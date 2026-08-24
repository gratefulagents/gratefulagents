def execute(data):
    consumed = False
    paid = data["allocation"] if data["eligible"] else 0
    if paid and data["reenter"] and not consumed:
        paid += data["allocation"]
    return {"paid": paid}
